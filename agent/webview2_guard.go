package main

// Microsoft Edge WebView2 runtime for the chat window.
//
// The end user's side of a chat is a WebView2 page (chat_windows.go). The
// Evergreen runtime ships with Windows 11 but is missing on Windows Server
// 2016 / 2019 / 2022, Windows 10 LTSC and images that never received it.
// This file is the platform-neutral part:
//
//   - detection result + persisted install state (configDir/webview2-state.json):
//     one install attempt at a time, back-off after a failure
//     (10 min / 30 min / 2 h / 6 h / 24 h, bypassed by a forced request),
//     an attempt launched by a previous process adopted on start (the
//     installer is a child process that outlives a service restart);
//   - the `webview2` block of the heartbeat (state shown on the device page,
//     read by the server to decide whether to send `webview2_install`);
//   - the install pipeline ("webview2_install" command): re-detect, verify
//     the Microsoft-signed Evergreen Bootstrapper shipped next to the agent
//     (Microsoft Authenticode + OriginalFilename pin, never re-signed), run it
//     as SYSTEM (per-machine install) with "/silent /install", then decide on
//     re-detection (never on the exit code alone).
//
// The server decides WHEN to install (platform setting: proactive / on
// demand / disabled); the agent only guards and executes. Windows parts:
// webview2_windows.go, stubs elsewhere (webview2_stub.go).

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// webview2BootstrapperName: the Evergreen Bootstrapper, installed by the
	// MSI next to oblireach-agent.exe (Program Files, admin-only).
	webview2BootstrapperName = "MicrosoftEdgeWebview2Setup.exe"
	// webview2ClientGUID: EdgeUpdate client id of the WebView2 Runtime.
	webview2ClientGUID = "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

	webview2InstallTimeout = 20 * time.Minute
	// After the installer exits, EdgeUpdate may register the runtime a bit
	// later: detection is polled this long.
	webview2DetectAfterWait = 60 * time.Second
	webview2DetectPoll      = 3 * time.Second
	// An attempt older than this is concluded from detection alone.
	webview2StaleInFlight = webview2InstallTimeout + 10*time.Minute
	// A cached detection older than this is refreshed by the next heartbeat.
	webview2DetectTTL = 10 * time.Minute
	// Floor between the starts of two attempts, forced or not.
	webview2MinGap = time.Minute
	// Identical "skipped" lines are logged at most this often.
	webview2SkipLogEvery = 10 * time.Minute

	webview2StatusMaxText = 300
)

// Back-off after a failed attempt; the last step repeats.
var webview2FailureSteps = []time.Duration{
	10 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 24 * time.Hour,
}

// Heartbeat states.
const (
	wv2StateOK          = "ok"          // runtime found
	wv2StateMissing     = "missing"     // not found, no attempt failed yet
	wv2StateInstalling  = "installing"  // an install attempt is running
	wv2StateFailed      = "failed"      // not found, the last attempt failed
	wv2StateBlocked     = "blocked"     // not found, an EdgeUpdate policy forbids its install
	wv2StateUnsupported = "unsupported" // not found, Windows too old for WebView2
	wv2StateUnknown     = "unknown"     // detection itself failed
)

// webview2Detection: what the service sees (same identity as the chat helper).
type webview2Detection struct {
	Version       string // "" = no runtime found
	Scope         string // machine | other | user_only | none | unknown
	PolicyBlocked bool   // EdgeUpdate policy forbids the WebView2 install
	Unsupported   bool   // Windows older than 10 / Server 2016
	Err           string // detection failed
	At            time.Time
}

func (d webview2Detection) installed() bool { return d.Version != "" }

// webview2Attempt: one run of the bootstrapper.
type webview2Attempt struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	StartedAt time.Time `json:"startedAt"`
	PID       int       `json:"pid,omitempty"`
	Force     bool      `json:"force,omitempty"`
}

type webview2PersistState struct {
	Failures      int              `json:"failures,omitempty"` // failed attempts in a row
	LastAttemptAt time.Time        `json:"lastAttemptAt,omitempty"`
	LastOutcome   string           `json:"lastOutcome,omitempty"` // installed | failed
	LastReason    string           `json:"lastReason,omitempty"`  // reason code of the last failure
	LastError     string           `json:"lastError,omitempty"`
	LastResultAt  time.Time        `json:"lastResultAt,omitempty"`
	NextAttemptAt time.Time        `json:"nextAttemptAt,omitempty"`
	Pending       *webview2Attempt `json:"pending,omitempty"` // installer launched, result not concluded yet
}

// webview2Result: outcome of one attempt.
type webview2Result struct {
	Installed bool
	Version   string
	Reason    string // bootstrapper_missing | signature | launch | timeout | network | access_denied | cancelled | installer_failed | exit_code | not_detected
	Detail    string
	ExitCode  int64 // -1 = unknown
}

func (r webview2Result) summary() string {
	if r.Installed {
		if r.Version != "" {
			return "installed (WebView2 " + r.Version + ")"
		}
		return "installed"
	}
	s := r.Reason
	if r.ExitCode >= 0 {
		s += fmt.Sprintf(" (installer exit code 0x%08X)", uint32(r.ExitCode))
	}
	if r.Detail != "" {
		s += ": " + r.Detail
	}
	return s
}

type webview2Decision struct {
	OK      bool
	Reason  string // in_progress | already_installed | unsupported | blocked | cooldown | min_gap
	Detail  string
	Log     bool
	Attempt *webview2Attempt
}

type webview2Guard struct {
	mu     sync.Mutex
	now    func() time.Time
	pathFn func() string // state file; "" = memory only
	loaded bool
	st     webview2PersistState

	inFlight      *webview2Attempt
	inFlightSince time.Time

	det *webview2Detection

	lastSkipKey string
	lastSkipLog time.Time
}

func newWebView2Guard(pathFn func() string) *webview2Guard {
	return &webview2Guard{now: time.Now, pathFn: pathFn}
}

func (g *webview2Guard) ensureLoadedLocked() {
	if g.loaded {
		return
	}
	g.loaded = true
	if g.pathFn == nil {
		return
	}
	p := g.pathFn()
	if p == "" {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var st webview2PersistState
	if json.Unmarshal(data, &st) == nil {
		g.st = st
	}
}

func (g *webview2Guard) saveLocked() {
	if g.pathFn == nil {
		return
	}
	p := g.pathFn()
	if p == "" {
		return
	}
	data, err := json.MarshalIndent(g.st, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, data, 0644) == nil {
		_ = os.Rename(tmp, p)
	}
}

// noteDetection stores a detection. A runtime found after failed attempts
// (installed by hand, by Windows Update…) clears the back-off.
func (g *webview2Guard) noteDetection(d webview2Detection) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLoadedLocked()
	cp := d
	g.det = &cp
	if d.installed() && (g.st.Failures > 0 || !g.st.NextAttemptAt.IsZero()) {
		g.st.Failures = 0
		g.st.NextAttemptAt = time.Time{}
		g.saveLocked()
	}
}

// detection returns the last detection (nil: none yet).
func (g *webview2Guard) detection() *webview2Detection {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.det == nil {
		return nil
	}
	cp := *g.det
	return &cp
}

func (g *webview2Guard) pendingActiveLocked(now time.Time) bool {
	return g.st.Pending != nil && now.Sub(g.st.Pending.StartedAt) < webview2StaleInFlight && now.Sub(g.st.Pending.StartedAt) > -time.Hour
}

func (g *webview2Guard) installingLocked(now time.Time) bool {
	if g.inFlight != nil && now.Sub(g.inFlightSince) < webview2StaleInFlight {
		return true
	}
	return g.inFlight == nil && g.pendingActiveLocked(now)
}

func (g *webview2Guard) skipLocked(reason, key, detail string, now time.Time) webview2Decision {
	key = reason + "|" + key
	logIt := key != g.lastSkipKey || now.Sub(g.lastSkipLog) >= webview2SkipLogEvery
	if logIt {
		g.lastSkipKey = key
		g.lastSkipLog = now
	}
	return webview2Decision{Reason: reason, Detail: detail, Log: logIt}
}

// begin decides whether a "webview2_install" request may start an attempt
// now (the caller re-detected just before). When OK, the caller owns the
// attempt and MUST end it with finish().
func (g *webview2Guard) begin(id, source string, force bool) webview2Decision {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLoadedLocked()
	now := g.now()

	if g.installingLocked(now) {
		cur := g.inFlight
		if cur == nil {
			cur = g.st.Pending
		}
		return g.skipLocked("in_progress", cur.ID,
			fmt.Sprintf("an install attempt is already running (id=%s, started %s)", cur.ID, cur.StartedAt.Format("15:04:05")), now)
	}
	if g.inFlight != nil {
		g.inFlight = nil // stuck: forget it (its late finish is ignored)
	}
	if g.det != nil && g.det.installed() {
		return g.skipLocked("already_installed", g.det.Version, "WebView2 "+g.det.Version+" is already installed", now)
	}
	if g.det != nil && g.det.Unsupported {
		return g.skipLocked("unsupported", "", "this version of Windows does not support the WebView2 runtime", now)
	}
	if !force && g.det != nil && g.det.PolicyBlocked {
		return g.skipLocked("blocked", "", "an EdgeUpdate policy forbids the WebView2 runtime install", now)
	}
	wait := time.Time{}
	if !force {
		wait = g.st.NextAttemptAt
	}
	if gap := g.st.LastAttemptAt.Add(webview2MinGap); gap.After(wait) {
		wait = gap
	}
	if wait.Sub(now) > 48*time.Hour {
		wait = time.Time{} // the clock went back: never stay blocked
	}
	if now.Before(wait) {
		reason := "cooldown"
		if force {
			reason = "min_gap"
		}
		detail := "next attempt not before " + wait.Format("15:04:05")
		if g.st.LastError != "" {
			detail += " (last error: " + g.st.LastError + ")"
		}
		return g.skipLocked(reason, wait.Format(time.RFC3339), detail, now)
	}

	att := &webview2Attempt{ID: id, Source: source, StartedAt: now, Force: force}
	cp := *att
	g.inFlight = &cp
	g.inFlightSince = now
	g.st.LastAttemptAt = now
	g.lastSkipKey = ""
	g.saveLocked()
	return webview2Decision{OK: true, Attempt: att, Log: true}
}

// launched persists the running attempt (with its PID): a service restart
// meanwhile (MSI upgrade) adopts it instead of starting a second installer.
func (g *webview2Guard) launched(att *webview2Attempt) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLoadedLocked()
	cp := *att
	g.st.Pending = &cp
	if g.inFlight != nil && g.inFlight.ID == att.ID {
		g.inFlight.PID = att.PID
	}
	g.saveLocked()
}

// adoptPending is called once at start: an attempt launched by the previous
// process becomes this process' in-flight attempt until it is concluded.
func (g *webview2Guard) adoptPending() *webview2Attempt {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLoadedLocked()
	if g.st.Pending == nil {
		return nil
	}
	held := *g.st.Pending
	g.inFlight = &held
	g.inFlightSince = g.now()
	if age := g.now().Sub(held.StartedAt); age >= 0 && age < webview2StaleInFlight {
		g.inFlightSince = held.StartedAt
	}
	cp := held
	return &cp
}

// finish ends att with res; returns the earliest time of the next automatic attempt.
func (g *webview2Guard) finish(att *webview2Attempt, res webview2Result) time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLoadedLocked()
	now := g.now()
	current := g.inFlight != nil && g.inFlight.ID == att.ID
	pending := g.st.Pending != nil && g.st.Pending.ID == att.ID
	if !current && !pending {
		return g.st.NextAttemptAt
	}
	if current {
		g.inFlight = nil
	}
	if pending {
		g.st.Pending = nil
	}
	if res.Installed {
		g.st.Failures = 0
		g.st.NextAttemptAt = time.Time{}
		g.st.LastOutcome = "installed"
		g.st.LastReason = ""
		g.st.LastError = ""
	} else {
		g.st.Failures++
		g.st.NextAttemptAt = now.Add(backoffStep(webview2FailureSteps, g.st.Failures))
		g.st.LastOutcome = "failed"
		g.st.LastReason = res.Reason
		g.st.LastError = capText(res.summary(), webview2StatusMaxText)
	}
	g.st.LastResultAt = now
	g.saveLocked()
	return g.st.NextAttemptAt
}

func (g *webview2Guard) snapshot() webview2PersistState {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLoadedLocked()
	st := g.st
	if st.Pending != nil {
		cp := *st.Pending
		st.Pending = &cp
	}
	return st
}

// webview2Status is the `webview2` block of the heartbeat (Windows only).
type webview2Status struct {
	State           string `json:"state"`             // ok | missing | installing | failed | blocked | unsupported | unknown
	Version         string `json:"version,omitempty"` // installed runtime
	Scope           string `json:"scope,omitempty"`   // machine | other | user_only | none | unknown
	Reason          string `json:"reason,omitempty"`  // why it is not usable / why the last attempt failed
	Installer       bool   `json:"installer"`         // the Microsoft bootstrapper ships with this agent
	InstallingSince string `json:"installingSince,omitempty"`
	Attempts        int    `json:"attempts,omitempty"` // failed attempts in a row
	LastError       string `json:"lastError,omitempty"`
	LastAttemptAt   string `json:"lastAttemptAt,omitempty"`
	LastResultAt    string `json:"lastResultAt,omitempty"`
	NextAttemptAt   string `json:"nextAttemptAt,omitempty"` // automatic attempts not before (a forced request only waits 1 min)
	CheckedAt       string `json:"checkedAt,omitempty"`
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// status builds the heartbeat block from the last detection (nil: none yet).
func (g *webview2Guard) status(installer bool) *webview2Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLoadedLocked()
	if g.det == nil {
		return nil
	}
	now := g.now()
	d := g.det
	s := &webview2Status{
		Version:   d.Version,
		Scope:     d.Scope,
		Installer: installer,
		CheckedAt: rfc3339(d.At),
	}
	installing := g.installingLocked(now)
	switch {
	case d.installed():
		s.State = wv2StateOK
	case installing:
		s.State = wv2StateInstalling
		since := g.inFlightSince
		if g.inFlight == nil && g.st.Pending != nil {
			since = g.st.Pending.StartedAt
		}
		s.InstallingSince = rfc3339(since)
	case d.Err != "":
		s.State = wv2StateUnknown
		s.Reason = "detect_error"
		s.LastError = capText(d.Err, webview2StatusMaxText)
	case d.Unsupported:
		s.State = wv2StateUnsupported
		s.Reason = "unsupported_os"
	case d.PolicyBlocked:
		s.State = wv2StateBlocked
		s.Reason = "blocked_by_policy"
	case g.st.LastOutcome == "failed" && g.st.Failures > 0:
		s.State = wv2StateFailed
		s.Reason = g.st.LastReason
	default:
		s.State = wv2StateMissing
		if d.Scope == "user_only" || d.Scope == "other" {
			s.Reason = "per_user_only"
		}
	}
	if !d.installed() {
		s.Attempts = g.st.Failures
		if s.LastError == "" && g.st.LastOutcome == "failed" {
			s.LastError = g.st.LastError
		}
		s.LastAttemptAt = rfc3339(g.st.LastAttemptAt)
		s.LastResultAt = rfc3339(g.st.LastResultAt)
		if !installing && g.st.NextAttemptAt.After(now) {
			s.NextAttemptAt = rfc3339(g.st.NextAttemptAt)
		}
	}
	return s
}

// statusKey: what matters to the server / device page (an immediate
// heartbeat is sent when it changes).
func (s *webview2Status) key() string {
	if s == nil {
		return ""
	}
	return strings.Join([]string{s.State, s.Version, s.Scope, s.Reason, fmt.Sprint(s.Installer, s.Attempts), s.NextAttemptAt, s.InstallingSince}, "|")
}

// ── Process-wide instance + pipeline ─────────────────────────────────────────

var webview2 = newWebView2Guard(func() string {
	if configDir == "" {
		return ""
	}
	return filepath.Join(configDir, "webview2-state.json")
})

// Hooks — Windows implementations in webview2_windows.go, stubs in
// webview2_stub.go, replaced in tests.
var (
	webview2Supported        = platformWebView2Supported
	webview2DetectFn         = platformWebView2Detect
	webview2ChatAltFn        = platformWebView2ChatAlternative
	webview2BootstrapperPath = platformWebView2BootstrapperPath
	webview2VerifyFn         = verifyWebView2Bootstrapper
	webview2LaunchFn         = launchWebView2Bootstrapper
	webview2AliveFn          = webview2InstallerAlive
	webview2Sleep            = time.Sleep
	webview2Now              = time.Now
)

var (
	webview2DetectMu  sync.Mutex
	webview2LastKeyMu sync.Mutex
	webview2LastKey   string
)

// webview2Refresh re-detects now (serialized) and returns the detection.
func webview2Refresh() webview2Detection {
	webview2DetectMu.Lock()
	d := webview2DetectFn()
	webview2DetectMu.Unlock()
	if d.At.IsZero() {
		d.At = webview2Now()
	}
	webview2.noteDetection(d)
	webview2NotifyIfChanged()
	return d
}

func webview2HasInstaller() bool {
	p := webview2BootstrapperPath()
	if p == "" {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// webview2HeartbeatStatus: the heartbeat block (nil off Windows). The
// detection is refreshed when older than webview2DetectTTL.
func webview2HeartbeatStatus() *webview2Status {
	if !webview2Supported() {
		return nil
	}
	if d := webview2.detection(); d == nil || webview2Now().Sub(d.At) >= webview2DetectTTL {
		webview2Refresh()
	}
	s := webview2.status(webview2HasInstaller())
	webview2LastKeyMu.Lock()
	webview2LastKey = s.key()
	webview2LastKeyMu.Unlock()
	return s
}

// webview2NotifyIfChanged asks for an immediate heartbeat when the state the
// server sees changed (start / end of an attempt, runtime appeared…).
func webview2NotifyIfChanged() {
	if !webview2Supported() {
		return
	}
	s := webview2.status(webview2HasInstaller())
	webview2LastKeyMu.Lock()
	changed := s.key() != webview2LastKey
	webview2LastKey = s.key() // one request per change (the heartbeat carries the latest state)
	webview2LastKeyMu.Unlock()
	if changed {
		requestHeartbeatNow()
	}
}

// webview2ChatCheck: before a chat helper is spawned. ok=false when the
// runtime is (still) missing — the chat cannot open.
func webview2ChatCheck() (ok bool, st *webview2Status) {
	if !webview2Supported() {
		return true, nil
	}
	d := webview2Refresh()
	s := webview2.status(webview2HasInstaller())
	if d.installed() || d.Err != "" {
		// A failed detection does not block the chat: the helper's own
		// handshake reports an honest failure if WebView2 cannot start.
		return true, s
	}
	// No WebView2 Runtime per machine, but the helper's loader may accept
	// something else (policy, Edge preview, the user's own install): tried,
	// its handshake tells the operator if it cannot start.
	if alt := webview2ChatAltFn(); alt != "" {
		log.Printf("webview2: no WebView2 Runtime per machine, but %s may serve the chat window: trying it", alt)
		return true, s
	}
	return false, s
}

// requestWebView2Install handles the "webview2_install" command: payload
// {source:"microsoft", force?}. Runs in the command's own goroutine.
func requestWebView2Install(cmd *command) {
	if !webview2Supported() {
		log.Printf("webview2: install request %s ignored: not applicable on this platform", cmd.ID)
		return
	}
	force, _ := cmd.Payload["force"].(bool)
	source := strings.TrimSpace(payloadString(cmd.Payload, "source"))
	if source == "" {
		source = "microsoft"
	}
	if source != "microsoft" {
		// Only the bootstrapper shipped with the agent is run (the server
		// download fallback is not implemented).
		log.Printf("webview2: install request %s ignored: unsupported source %q", cmd.ID, source)
		return
	}
	webview2Refresh()
	d := webview2.begin(cmd.ID, source, force)
	if !d.OK {
		if d.Log {
			log.Printf("webview2: install requested (id=%s, force=%v) — skipped: %s", cmd.ID, force, d.Detail)
		}
		webview2NotifyIfChanged()
		return
	}
	att := d.Attempt
	requestHeartbeatNow() // the device page shows "installing" at once
	res := runWebView2Install(att)
	next := webview2.finish(att, res)
	logWebView2Result(att, res, next)
	webview2NotifyIfChanged()
}

func logWebView2Result(att *webview2Attempt, res webview2Result, next time.Time) {
	if res.Installed {
		log.Printf("webview2: result — OK: %s (id=%s)", res.summary(), att.ID)
		return
	}
	log.Printf("webview2: result — FAILED (id=%s): %s; next automatic attempt not before %s (EdgeUpdate log: %%ProgramData%%\\Microsoft\\EdgeUpdate\\Log)",
		att.ID, res.summary(), next.Format("2006-01-02 15:04:05"))
}

// runWebView2Install performs one attempt: bootstrapper present and signed
// by Microsoft, launched as this (SYSTEM) service, waited for, then the
// runtime re-detected.
func runWebView2Install(att *webview2Attempt) webview2Result {
	path := webview2BootstrapperPath()
	if path == "" || !webview2HasInstaller() {
		return webview2Result{Reason: "bootstrapper_missing", ExitCode: -1,
			Detail: webview2BootstrapperName + " is not installed next to the agent (update ObliReach)"}
	}
	log.Printf("webview2: start install attempt (id=%s, force=%v) with %s", att.ID, att.Force, path)
	lock, signer, err := webview2VerifyFn(path)
	if err != nil {
		return webview2Result{Reason: "signature", ExitCode: -1, Detail: err.Error()}
	}
	pid, wait, err := webview2LaunchFn(path, lock)
	if err != nil {
		return webview2Result{Reason: "launch", ExitCode: -1, Detail: err.Error()}
	}
	att.PID = pid
	webview2.launched(att)
	log.Printf("webview2: installer launched (PID %d, signed by %q): %s /silent /install", pid, signer, path)

	code, timedOut, werr := wait(webview2InstallTimeout)
	if werr != nil {
		log.Printf("webview2: waiting for the installer: %v", werr)
	}
	d := webview2WaitDetected()
	if d.installed() {
		if code != 0 && !timedOut {
			log.Printf("webview2: installer exit code 0x%08X, but the runtime is installed", uint32(code))
		}
		return webview2Result{Installed: true, Version: d.Version, ExitCode: code}
	}
	if timedOut {
		return webview2Result{Reason: "timeout", ExitCode: -1,
			Detail: fmt.Sprintf("the installer did not finish within %s (stopped)", webview2InstallTimeout)}
	}
	if werr != nil {
		return webview2Result{Reason: "launch", ExitCode: -1, Detail: werr.Error()}
	}
	reason := webview2ExitReason(code)
	detail := "runtime still not detected after the installer exited"
	if code == 0 {
		reason = "not_detected"
	}
	return webview2Result{Reason: reason, ExitCode: code, Detail: detail}
}

// webview2WaitDetected polls detection for webview2DetectAfterWait.
func webview2WaitDetected() webview2Detection {
	deadline := webview2Now().Add(webview2DetectAfterWait)
	for {
		d := webview2Refresh()
		if d.installed() || !webview2Now().Before(deadline) {
			return d
		}
		webview2Sleep(webview2DetectPoll)
	}
}

// webview2ExitReason: reason code of a bootstrapper exit code (HRESULT).
func webview2ExitReason(code int64) string {
	switch uint32(code) {
	case 0x80072EE2, 0x80072EE7, 0x80072EFD, 0x80072EFE, 0x80072F8F, 0x80072F7D, 0x80072F0D, 0x80072F06, 0x80072EFF, 0x80072F78:
		return "network"
	case 0x80070005:
		return "access_denied"
	case 0x800704C7:
		return "cancelled"
	case 0x80040C01:
		return "installer_failed"
	}
	return "exit_code"
}

// webview2OnStart: at service start, conclude an attempt launched by the
// previous process (never start a second installer while it runs), then a
// first detection. Runs in its own goroutine.
func webview2OnStart() {
	if !webview2Supported() {
		return
	}
	att := webview2.adoptPending()
	if att != nil {
		log.Printf("webview2: adopting the install attempt started by the previous process (id=%s, PID %d, started %s)",
			att.ID, att.PID, att.StartedAt.Format("15:04:05"))
		deadline := att.StartedAt.Add(webview2InstallTimeout + time.Minute)
		for att.PID > 0 && webview2AliveFn(att.PID) && webview2Now().Before(deadline) {
			if webview2Refresh().installed() {
				break
			}
			webview2Sleep(5 * time.Second)
		}
		d := webview2WaitDetected()
		res := webview2Result{Installed: d.installed(), Version: d.Version, ExitCode: -1}
		if !res.Installed {
			res.Reason = "not_detected"
			res.Detail = "the runtime is not detected after the installer started by the previous process"
		}
		next := webview2.finish(att, res)
		logWebView2Result(att, res, next)
	}
	d := webview2Refresh()
	switch {
	case d.installed():
		log.Printf("webview2: runtime %s found (%s)", d.Version, d.Scope)
	case d.Err != "":
		log.Printf("webview2: detection failed: %s", d.Err)
	default:
		log.Printf("webview2: runtime NOT found (scope=%s, policyBlocked=%v, unsupportedOS=%v, bootstrapper shipped=%v) — the chat window cannot open until it is installed",
			d.Scope, d.PolicyBlocked, d.Unsupported, webview2HasInstaller())
	}
}
