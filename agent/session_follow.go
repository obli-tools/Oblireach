package main

// Session follow (symptom: "Reach does not find the session I just logged
// into"). The service (session 0) keeps ONE relay WebSocket — the same approved
// remote_sessions row, the same browser — for the whole session, and re-spawns
// the capture helper in the right Windows session whenever the console session
// changes (logon at the login screen, a console re-attach of a disconnected RDP
// session, fast user switching, logoff). The viewer stays connected and shows a
// short "following the session…" notice instead of dropping and reconnecting.
//
// This file is PURE Go (no cgo, no build tag) so the decision state machine and
// the replay/nonce helpers are unit-tested without a real Windows session
// (session_follow_test.go). The Windows glue is remote_session_windows.go
// (supervisor + legs), session_watch_windows.go (the WTS snapshot + SCM
// SESSIONCHANGE wakeup) and service_windows.go (the service accepts
// SERVICE_CONTROL_SESSIONCHANGE).

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// helperNonce is the one-time token the supervisor put on the helper's command
// line (--nonce). The helper sends it as its first pipe frame (PipeTypeHello) so
// the supervisor's per-leg listener only trusts the process it spawned: the
// listener is 127.0.0.1:<port> and, for the ~15 s it is open, ANY local process
// could otherwise connect and receive the operator's keystrokes (e.g. a password
// typed on the login screen). The helper runs as SYSTEM, so a standard user
// cannot read its command line to steal the nonce.
var helperNonce string

// switchDXGIWaitMs is what the supervisor passes (--dxgi-wait) to a helper it
// spawns for a session switch; maxDXGIWaitMs bounds the flag.
const (
	switchDXGIWaitMs = 5000
	maxDXGIWaitMs    = 10000
)

// followDebounce: a switch / no_display / error decision must be seen on two
// samples at least this far apart before it is acted on. The console session id
// flips for a moment during a re-attach (the dying login session is still
// reported), so without this the supervisor would spawn into a session that is
// going away and respawn in a loop.
const followDebounce = 400 * time.Millisecond

// followMode: what Windows session the supervisor tracks for the whole stream.
type followMode int

const (
	// followConsole tracks the physical console (like TeamViewer): when the
	// console user's session moves away over RDP, the operator sees the login
	// screen; when someone signs in at the console, the operator follows them.
	followConsole followMode = iota
	// followPinned tracks one session id the operator chose explicitly.
	followPinned
)

// openModeFor decides the follow mode at open time from the requested session id
// and the console id at that moment: console when nothing (or 0/negative) was
// asked, or when the request IS the current console; otherwise the pinned id.
func openModeFor(requested, console int) (followMode, int) {
	if requested <= 0 || requested == console {
		return followConsole, 0
	}
	return followPinned, requested
}

// followRequestFor is the request the supervisor derives its mode from, given
// what the operator asked for and what resolveTargetSession resolved it to. An
// explicit id that did not survive resolution — the recent-console safety net
// mapped a vanished (former console) id to the current console — follows the
// CONSOLE: pinning the vanished id would end the stream a second later with
// session_logged_off.
func followRequestFor(requested, resolved int) int {
	if requested > 0 && resolved != requested {
		return -1
	}
	return requested
}

// followAction is what the controller decided for the current sample.
type followAction int

const (
	actionStay      followAction = iota // nothing to do
	actionWait                          // no usable console right now (0xFFFFFFFF, or a session being torn down): wait
	actionSwitch                        // (re-)spawn the helper in `session`
	actionNoDisplay                     // the pinned session is disconnected: no screen
	actionErrorEnd                      // end the stream with `errCode`
)

// followDecision is one outcome of the state machine.
type followDecision struct {
	action  followAction
	session int    // target session (actionSwitch / actionNoDisplay)
	hasUser bool   // the target session has a logged-in user (Default vs Winlogon desktop)
	user    string // the target session's user name (shown to the operator)
	ident   string // DOMAIN\user, lower-case (same-user rules: re-notify, input block)
	reason  string // logon | logoff | console_change | reconnected | helper_restart | disconnected
	errCode string // actionErrorEnd: session_logged_off | internal
}

// legInfo is the capture helper currently serving the stream, from the
// controller's point of view (the OS handles live in the supervisor's *leg).
type legInfo struct {
	session int
	hasUser bool
	user    string
	ident   string
}

// wtsInfo is what a snapshot knows about one Windows session.
type wtsInfo struct {
	exists bool
	state  string // "Active" | "Connected" | "ConnectQuery" | "Disconnected" | "" (not enumerated) ...
	user   string
	ident  string // userIdent(domain, user)
}

// userIdent is the identity the same-user rules compare: DOMAIN\user,
// case-insensitive. The short WTS user name alone would take the local
// ".\Administrator" and "CORP\Administrator" for the same person.
func userIdent(domain, user string) string {
	if user == "" {
		return ""
	}
	if domain == "" {
		return strings.ToLower(user)
	}
	return strings.ToLower(domain + `\` + user)
}

// sameUser compares two identities (userIdent). When one side has no domain
// (WTSDomainName could not be read for that sample), only the names are
// compared, so a missing domain never makes the same person look like another.
func sameUser(a, b string) bool {
	if a == b {
		return true
	}
	ia, ib := strings.LastIndexByte(a, '\\'), strings.LastIndexByte(b, '\\')
	if (ia < 0) != (ib < 0) {
		return a[ia+1:] == b[ib+1:]
	}
	return false
}

// followSnapshot is one observation of the WTS state, built by the watcher.
type followSnapshot struct {
	consoleID int     // WTSGetActiveConsoleSessionId, or -1 for 0xFFFFFFFF
	console   wtsInfo // the console session (best-effort user/state)
	pinned    wtsInfo // the pinned session (followPinned mode)
	now       time.Time
}

// helperGoneSettle: after a helper died, no respawn is decided for this long
// (on top of the two-sample debounce). A logoff / console re-attach breaks the
// pipe BEFORE WTS reports the new state; deciding at once would respawn into
// the session being torn down.
const helperGoneSettle = time.Second

// followController is the debounced decision state machine. It lives in the
// supervisor goroutine only (the watcher hands it snapshots), so it needs no
// lock.
type followController struct {
	mode      followMode
	pinned    int // followPinned target
	leg       legInfo
	noDisplay bool // currently reporting no_display (pinned session disconnected)

	// legGone: no helper serves leg.session right now — it died (a crash, or
	// the session was torn down under it) or a switch could not bring one up.
	// The next debounced samples decide where to (re)spawn; the supervisor
	// bounds the whole wait (switchDeadline).
	legGone     bool
	goneAt      time.Time
	retryReason string // the reason shown when the gone leg is brought back

	// debounce
	pending     followDecision
	pendingAt   time.Time
	havePending bool

	restart helperRestartBudget
}

func newFollowController(mode followMode, pinned int, leg legInfo) *followController {
	return &followController{mode: mode, pinned: pinned, leg: leg}
}

// rawDecision is the undebounced decision for one snapshot.
func (c *followController) rawDecision(snap followSnapshot) followDecision {
	if c.mode == followPinned {
		return c.rawPinned(snap)
	}
	return c.rawConsole(snap)
}

func (c *followController) rawConsole(snap followSnapshot) followDecision {
	if snap.consoleID < 0 {
		return followDecision{action: actionWait}
	}
	target := snap.consoleID
	cu := snap.console
	if cu.state == "Disconnected" {
		// The console id still names a session that is being torn down (a
		// console re-attach): never spawn into it, wait for the new console.
		return followDecision{action: actionWait}
	}
	sw := func(reason string) followDecision {
		return followDecision{action: actionSwitch, session: target, hasUser: cu.user != "", user: cu.user, ident: cu.ident, reason: reason}
	}
	if target != c.leg.session {
		return sw(consoleReason(c.leg, cu))
	}
	// Same session id as the leg. A lock / unlock / UAC changes neither the
	// console id nor the user, so both fall through to actionStay: the native
	// capture thread already follows the secure desktop.
	if cu.user != "" && !c.leg.hasUser {
		// The login session the helper was spawned on now has a user: re-spawn
		// on the Default desktop so the "Remote session active" watermark, the
		// REC pill and the input-block are on the desktop the user sees.
		return sw("logon")
	}
	if cu.user == "" && c.leg.hasUser {
		return sw("logoff")
	}
	if cu.user != "" && c.leg.hasUser && !sameUser(cu.ident, c.leg.ident) {
		// The session id was reused by ANOTHER user: a new person to notify,
		// and never the previous user's input block.
		return sw("logon")
	}
	if c.legGone {
		return sw(c.goneReason())
	}
	return followDecision{action: actionStay}
}

func (c *followController) rawPinned(snap followSnapshot) followDecision {
	p := snap.pinned
	loggedOff := followDecision{action: actionErrorEnd, errCode: "session_logged_off", reason: "logoff"}
	if !p.exists {
		return loggedOff
	}
	switch p.state {
	case "Disconnected":
		if c.noDisplay {
			return followDecision{action: actionStay}
		}
		return followDecision{action: actionNoDisplay, session: c.pinned, user: p.user, ident: p.ident, reason: "disconnected"}
	case "Active", "Connected", "ConnectQuery":
		sw := func(reason string) followDecision {
			return followDecision{action: actionSwitch, session: c.pinned, hasUser: p.user != "", user: p.user, ident: p.ident, reason: reason}
		}
		// A pinned session is bound to the user in it once there is one: if
		// that user is gone (signed out, the id now shows a login prompt or
		// belongs to someone else), the session the operator picked no longer
		// exists — never keep watching whoever is there now.
		if c.leg.hasUser && (p.user == "" || !sameUser(p.ident, c.leg.ident)) {
			return loggedOff
		}
		if p.user != "" && !c.leg.hasUser {
			// A user signed in to the pinned login-prompt session: re-spawn on
			// the Default desktop (watermark / REC pill visible, notified).
			return sw("logon")
		}
		if c.noDisplay || c.leg.session != c.pinned {
			return sw("reconnected")
		}
		if c.legGone {
			return sw(c.goneReason())
		}
		return followDecision{action: actionStay}
	default:
		return followDecision{action: actionStay}
	}
}

func (c *followController) goneReason() string {
	if c.retryReason != "" {
		return c.retryReason
	}
	return "helper_restart"
}

// consoleReason names why the console changed, from the user-presence
// transition. "reconnected" (the new console session existed as Disconnected)
// is not distinguished from "logon" here — both are shown as a session change to
// the operator and the wording is cosmetic.
func consoleReason(leg legInfo, cu wtsInfo) string {
	newUser := cu.user != ""
	switch {
	case newUser && !leg.hasUser:
		return "logon"
	case !newUser && leg.hasUser:
		return "logoff"
	case newUser && leg.hasUser && !sameUser(cu.ident, leg.ident):
		return "logon" // a different user took the console (fast user switching)
	default:
		return "console_change"
	}
}

// evaluate feeds one snapshot and returns the decision plus whether it is
// committed (debounced). A committed switch / no_display / error should be
// acted on; stay / wait and an un-committed decision should not. While the leg
// is gone, nothing commits before helperGoneSettle.
func (c *followController) evaluate(snap followSnapshot) (followDecision, bool) {
	raw := c.rawDecision(snap)
	if raw.action == actionStay || raw.action == actionWait {
		c.havePending = false
		return raw, false
	}
	if c.havePending && c.pending.action == raw.action && c.pending.session == raw.session && c.pending.errCode == raw.errCode {
		if snap.now.Sub(c.pendingAt) >= followDebounce && (!c.legGone || snap.now.Sub(c.goneAt) >= helperGoneSettle) {
			return raw, true
		}
		return raw, false
	}
	c.havePending = true
	c.pending = raw
	c.pendingAt = snap.now
	return raw, false
}

// appliedSwitch records that the supervisor moved the leg to dec.session.
func (c *followController) appliedSwitch(dec followDecision) {
	c.leg = legInfo{session: dec.session, hasUser: dec.hasUser, user: dec.user, ident: dec.ident}
	c.noDisplay = false
	c.legGone = false
	c.retryReason = ""
	c.havePending = false
}

// switchFailed records that a switch to dec.session could not bring a helper
// up: the target becomes the (gone) leg, so the next debounced samples retry it
// — or go wherever the session went meanwhile — until the supervisor's
// switchDeadline ends the stream.
func (c *followController) switchFailed(dec followDecision, now time.Time) {
	c.leg = legInfo{session: dec.session, hasUser: dec.hasUser, user: dec.user, ident: dec.ident}
	c.noDisplay = false
	if !c.legGone {
		c.goneAt = now
	}
	c.legGone = true
	c.retryReason = dec.reason
	c.havePending = false
}

// appliedNoDisplay records that the supervisor is now waiting for the pinned
// session to come back (no capture running).
func (c *followController) appliedNoDisplay() {
	c.noDisplay = true
	c.havePending = false
}

// helperGone records that the current leg's helper died (pipe broke). It does
// NOT decide the respawn: the debounced samples do, after helperGoneSettle, so
// a logoff / re-attach that breaks the pipe before WTS changes is followed
// rather than respawned into the dying session. Returns false when the
// helper died too often (helperRestartMax per helperRestartWindow): the stream
// must end (internal) instead of looping.
func (c *followController) helperGone(now time.Time) bool {
	if !c.restart.allow(now) {
		return false
	}
	c.legGone = true
	c.goneAt = now
	c.retryReason = "helper_restart"
	c.havePending = false
	return true
}

// helperRestartBudget allows at most helperRestartMax helper deaths (each one
// followed by a respawn) per helperRestartWindow (a sliding window), so a helper
// that dies right after its start cannot loop forever.
const (
	helperRestartMax    = 5
	helperRestartWindow = time.Minute
)

type helperRestartBudget struct {
	at []time.Time
}

func (b *helperRestartBudget) allow(now time.Time) bool {
	cutoff := now.Add(-helperRestartWindow)
	kept := b.at[:0]
	for _, t := range b.at {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	b.at = kept
	if len(b.at) >= helperRestartMax {
		return false
	}
	b.at = append(b.at, now)
	return true
}

// ── One-time leg nonce + capture-helper command line ────────────────────────

var nonceRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
var legAddrRe = regexp.MustCompile(`^127\.0\.0\.1:\d{1,5}$`)

// newLegNonce returns a 128-bit hex token for a leg's listener.
func newLegNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func validNonce(s string) bool { return nonceRe.MatchString(s) }

// captureHelperCmdLine builds the supervised capture-helper command line. addr
// is the leg's own loopback listener, nonce the one-time token, genBase the
// starting view-generation so gens never rewind across legs, dxgiWaitMs how
// long the helper keeps retrying DXGI before a fallback (0 = no wait: the first
// leg; switchDXGIWaitMs for a session switch). Every field is validated so
// nothing operator- or session-derived can inject arguments (the same rule as
// chatHelperCmdLine). noFiles adds --no-files: the Obliance server did not
// permit file transfers in this session (files_gate.go).
func captureHelperCmdLine(exe, addr, nonce string, genBase, dxgiWaitMs int, noFiles bool) (string, error) {
	if !legAddrRe.MatchString(addr) {
		return "", fmt.Errorf("follow: invalid helper addr %q", addr)
	}
	if !validNonce(nonce) {
		return "", fmt.Errorf("follow: invalid nonce")
	}
	if genBase < 0 {
		return "", fmt.Errorf("follow: invalid gen-base")
	}
	if dxgiWaitMs < 0 || dxgiWaitMs > maxDXGIWaitMs {
		return "", fmt.Errorf("follow: invalid dxgi-wait")
	}
	line := fmt.Sprintf(`"%s" --capture-helper --addr=%s --follow --nonce=%s --gen-base=%d`, exe, addr, nonce, genBase)
	if dxgiWaitMs > 0 {
		line += fmt.Sprintf(" --dxgi-wait=%d", dxgiWaitMs)
	}
	if noFiles {
		line += " --no-files"
	}
	return line, nil
}

// legGenBase spaces each leg's view-generation range so an old view's late input
// (its gen) can never match a new leg's view: gens stay strictly increasing
// across legs. One leg would have to emit over a million view changes to overrun
// its slice, which cannot happen.
func legGenBase(legIndex int) int { return legIndex << 20 }

// ── Replayed stream state (across a leg switch) ─────────────────────────────

// streamStateCache remembers the viewer-driven state that a fresh helper does
// not know: the codec picked, the view (monitor / mosaic), recording, the
// local-input block, and the file transfers in flight. The relay reader
// (browser → service) records it as it passes; on a switch the supervisor
// replays it to the new helper so the picture, the REC pill and the block come
// back, and warns the viewer about transfers the switch cut. Touched from the
// relay reader and the leg reader, so it holds its own lock.
type streamStateCache struct {
	mu         sync.Mutex
	recording  bool
	codec      string // last set_codec (h264 = nothing to replay)
	viewRaw    []byte // last set_view / set_monitor payload, replayed verbatim
	inputBlock bool
	blockUser  string // identity (userIdent) watched when the block was set (same-user rule)
	blank      bool   // the viewer's blank screen is on (blank_screen.go): never replayed
	tids       map[uint32]struct{}
}

func newStreamStateCache() *streamStateCache {
	return &streamStateCache{tids: map[uint32]struct{}{}}
}

// observeToHelper records a browser → helper JSON control frame. currentIdent
// is the identity (userIdent) of the leg's user it was aimed at, for the
// same-user input-block rule.
func (c *streamStateCache) observeToHelper(payload []byte, currentIdent string) {
	var m struct {
		Type      string  `json:"type"`
		Codec     string  `json:"codec"`
		Recording bool    `json:"recording"`
		Block     bool    `json:"block"`
		On        bool    `json:"on"`
		Tid       *uint32 `json:"tid"`
	}
	if json.Unmarshal(payload, &m) != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch m.Type {
	case "set_codec":
		c.codec = m.Codec
	case "set_view", "set_monitor":
		c.viewRaw = append([]byte(nil), payload...)
	case "set_recording":
		c.recording = m.Recording
	case "set_blank_screen":
		c.blank = m.On
	case "set_input_block":
		c.inputBlock = m.Block
		if m.Block {
			c.blockUser = currentIdent
		} else {
			c.blockUser = ""
		}
	case "file_begin", "file_get":
		if m.Tid != nil {
			c.tids[*m.Tid] = struct{}{}
		}
	case "file_cancel":
		if m.Tid != nil {
			delete(c.tids, *m.Tid)
		}
	}
}

// observeFromHelper records a helper → browser JSON control frame:
//   - file_done / file_error clear a finished transfer, so a completed one is
//     not reported as cut;
//   - input_block_status{blocked:false} clears the replayed block. The helper
//     says so when the LOCAL user lifted it with the escape hatch
//     (Ctrl+Alt+Del, lock, UAC) or when it could not be applied: the block
//     must then not come back on its own at the next switch — only an operator
//     may block again (input_block.go).
func (c *streamStateCache) observeFromHelper(payload []byte) {
	var m struct {
		Type    string  `json:"type"`
		Tid     *uint32 `json:"tid"`
		Blocked *bool   `json:"blocked"`
		Active  *bool   `json:"active"`
	}
	if json.Unmarshal(payload, &m) != nil {
		return
	}
	switch m.Type {
	case "file_done", "file_error":
		if m.Tid != nil {
			c.mu.Lock()
			delete(c.tids, *m.Tid)
			c.mu.Unlock()
		}
	case "input_block_status":
		if m.Blocked != nil && !*m.Blocked {
			c.mu.Lock()
			c.inputBlock = false
			c.blockUser = ""
			c.mu.Unlock()
		}
	case "blank_screen_status":
		if m.Active != nil {
			c.mu.Lock()
			c.blank = *m.Active
			c.mu.Unlock()
		}
	}
}

// takeBlank reports whether the viewer's blank screen was on and clears it: a
// session switch never carries it into another session (the new helper starts
// without it; the supervisor tells the viewer blank_screen_status off).
func (c *streamStateCache) takeBlank() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	was := c.blank
	c.blank = false
	return was
}

// transferStartTid reports whether a browser → helper frame starts a file
// transfer (file_begin / file_get) and its tid. While no helper runs (a switch
// in progress) such a request is answered at once with file_error
// {session_changed} instead of being dropped silently.
func transferStartTid(payload []byte) (uint32, bool) {
	var m struct {
		Type string  `json:"type"`
		Tid  *uint32 `json:"tid"`
	}
	if json.Unmarshal(payload, &m) != nil || m.Tid == nil {
		return 0, false
	}
	if m.Type == "file_begin" || m.Type == "file_get" {
		return *m.Tid, true
	}
	return 0, false
}

// takeTids returns the transfers still in flight and clears them (a switch cuts
// them; the supervisor tells the viewer file_error{session_changed}).
func (c *streamStateCache) takeTids() []uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tids) == 0 {
		return nil
	}
	out := make([]uint32, 0, len(c.tids))
	for tid := range c.tids {
		out = append(out, tid)
	}
	c.tids = map[uint32]struct{}{}
	return out
}

// replayMessages is the ordered list of pipe control messages to send a fresh
// helper so it matches the state the viewer already thinks it has. newIdent is
// the identity (userIdent) of the new session's user: when it is not the one the
// input block was set for, the block is dropped (the supervisor tells the viewer
// it is off).
func (c *streamStateCache) replayMessages(newIdent string) (msgs [][]byte, blockDropped bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Recording state always (the pill defaults to LIVE; a REC session must get
	// its pill back — and a non-recording one must not inherit a stale REC).
	if b, err := json.Marshal(map[string]interface{}{"type": "set_recording", "recording": c.recording}); err == nil {
		msgs = append(msgs, b)
	}
	if c.codec != "" && c.codec != "h264" {
		if b, err := json.Marshal(map[string]string{"type": "set_codec", "codec": c.codec}); err == nil {
			msgs = append(msgs, b)
		}
	}
	if len(c.viewRaw) > 0 {
		msgs = append(msgs, append([]byte(nil), c.viewRaw...))
	}
	if c.inputBlock {
		if c.blockUser != "" && sameUser(newIdent, c.blockUser) {
			if b, err := json.Marshal(map[string]interface{}{"type": "set_input_block", "block": true}); err == nil {
				msgs = append(msgs, b)
			}
		} else {
			// A different user is at the keyboard now: do not freeze them.
			c.inputBlock = false
			c.blockUser = ""
			blockDropped = true
		}
	}
	return msgs, blockDropped
}
